package exec

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

func (e *Executor) runStep(ctx context.Context, runID, stepID string, step Step, index, total int, depResults map[string]*StepResult) *StepResult {
	// Resolve agent.
	var agent AgentConfig
	if step.Agent != "" && e.Workflow.Agents != nil {
		if a, ok := e.Workflow.Agents[step.Agent]; ok {
			agent = a
		}
	}
	// when this executor is nested (namespacePrefix set),
	// shadow the local stepID to its namespaced form. ALL uses inside
	// runStep (Router.RegisterInbox, mailbox keys, runner.StepID,
	// event StepID emissions, push events to coord, delegation
	// registry) automatically use namespaced. The original bare ID is
	// preserved as `bareStepID` for the returned sr.ID (so the outer
	// aggregator can look up results by the declared inner step ID).
	bareStepID := stepID
	if e.namespacePrefix != "" {
		stepID = e.namespacePrefix + "." + stepID
	}
	// register delegation in root router so coord can address
	// this step (using its namespaced ID) via root.Send. Cleanup on
	// runStep return so subsequent iterations (or workflow end) don't
	// see stale routing. Skipped for outermost executor (no separate
	// root) and when no router wired.
	if e.RootRouter != nil && e.RootRouter != e.Router && e.Router != nil {
		e.RootRouter.RegisterDelegate(stepID, e.Router)
		defer e.RootRouter.UnregisterDelegate(stepID)
	}

	// Step isolation: Setup before execution, Cleanup deferred after.
	// workDir overrides baseDir for resolving context file paths when isolation is active.
	var isolationWorkDir string
	if e.Isolation != nil {
		workDir, setupErr := e.Isolation.Setup(ctx, runID, stepID)
		if setupErr != nil {
			return &StepResult{ID: bareStepID, Status: spec.StepFailed, Error: fmt.Errorf("isolation setup: %w", setupErr)}
		}
		defer func() {
 // Cleanup must run even if the step fails or panics.
			if cleanupErr := e.Isolation.Cleanup(ctx, runID, stepID); cleanupErr != nil {
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventError,
						Timestamp: time.Now(),
						RunID:     runID,
						StepID:    stepID,
						Error:     fmt.Errorf("isolation cleanup: %w", cleanupErr),
					})
				} else {
					slog.WarnContext(ctx, "isolation cleanup failed",
						"err", cleanupErr,
						"run_id", runID,
						"step_id", stepID,
					)
				}
			}
		}()
		if workDir != "" {
			isolationWorkDir = workDir
		}
	}

	// Start step trace span. EndSpan is deferred to cover all return paths.
	// Skip if called from within a loop iteration (loop manages its own spans).
	// Include steps have their own tracing in runIncludeStep.
	if e.Tracer != nil && !shouldSkipStepTrace(ctx) {
		attrs := map[string]string{"zenflow.step.id": stepID}
		if step.Agent != "" {
			attrs["zenflow.step.agent"] = step.Agent
		}
		ctx = e.Tracer.StartSpan(ctx, "zenflow.step", attrs)
	}
	var stepTraceErr error
	defer func() {
		if e.Tracer != nil && !shouldSkipStepTrace(ctx) {
			e.Tracer.EndSpan(ctx, stepTraceErr)
		}
	}()

	// Use isolation workDir for context file resolution when available,
	// otherwise fall back to the workflow's base directory.
	baseDir := e.Workflow.BaseDir
	if isolationWorkDir != "" {
		baseDir = isolationWorkDir
	}

	// Precedence (high → low): ForceModel (WithForceModel CLI override),
	// Step.Model, agent.Model, DefaultModel. cmp.Or returns the first
	// non-zero argument.
	model := cmp.Or(e.ForceModel, step.Model, agent.Model, e.DefaultModel)

	// P7.7.7: Transform dep results before prompt assembly when OutputTransform is set.
	// This allows consumers to implement smart truncation/compaction based on the
	// target model's context window (e.g., MiniMax 196K vs GPT-5 128K).
	if e.OutputTransform != nil && len(depResults) > 0 {
		for depID, sr := range depResults {
			if sr == nil || sr.Status != spec.StepCompleted {
				continue
			}
 // follow-up: PreserveContent opts the dep out of
 // the OutputTransform too. Without this, CLI's default
 // TokenBudgetTransformer (8KB MaxBytesPerDep - even stricter
 // than the 16KB maxDepContentBytes cap already
 // bypassed) truncates cumulative loop content BEFORE
 // writeDepSection sees it. The user's debate-until.yaml
 // repro showed `[truncated for context limit]` in verdict
 // even after because the CLI install adds the
 // transformer at cmd/zenflow/main.go:555 unconditionally.
			if sr.PreserveContent {
				continue
			}
			newContent, newResult := e.OutputTransform.TransformStepOutput(depID, sr.Content, sr.Result, model)
			if newContent != sr.Content || newResult != nil {
 // Create a shallow copy to avoid mutating the shared StepResult.
				transformed := *sr
				transformed.Content = newContent
				if newResult != nil {
					transformed.Result = newResult
				}
				depResults[depID] = &transformed
			}
		}
	}

	// If called from a forEach iteration, use the forEach-aware prompt assembler.
	var prompt string
	var attachments []provider.Part
	if fe := getForEachCtx(ctx); fe != nil {
		prompt, attachments = AssemblePromptWithForEach(agent, step, baseDir, depResults, fe)
	} else {
		prompt, attachments = AssemblePrompt(agent, step, baseDir, depResults)
	}

	// coord==nil blanket-context fallback. When WithFlowContext
	// supplied a non-empty string AND no coordinator runner is installed
	// to curate per-step forwards, prepend the context to every step's
	// effective user prompt so the LLM sees the use-case input directly.
	// The "[Flow Context]" marker keeps the section identifiable and is
	// also asserted as a sentinel by the no-context tests.
	if e.Coordinator == nil && e.FlowContext != "" {
		prompt = "[Flow Context]\n" + e.FlowContext + "\n\n" + prompt
	}

	tools := FilterTools(e.Runner.tools, agent.Tools, agent.DisallowedTools)

	// Register per-step mailbox slot when MessageRouter is available
	// This marks the step as live so coordinator Sends route into the
	// mailbox; defer Close marks it terminal so post-completion Sends
	// emit "target-terminal" drops.
	if e.Router != nil {
		e.Router.RegisterInbox(stepID)
 // NOTE: Router.Close is deferred BELOW (after waitForStepTermination)
 // so that - in LIFO order - it fires BEFORE the wait. See the
 // "B1 fix: defer order" comment near waitForStepTermination.
	}

	// Auto-inject shared memory tools when SharedMem is configured.
	// Creates per-step shared memory tools with the agent name as namespace.
	if e.SharedMem != nil {
		agentName := cmp.Or(step.Agent, stepID)
		smTools := NewSharedMemoryTools(e.SharedMem, agentName)
 // Auto-inject: always include shared memory tools unless explicitly disallowed.
		filteredSM := FilterTools(smTools, nil, agent.DisallowedTools)
		tools = append(tools, filteredSM...)
	}

	// Build step-level runner with combined tools, optional inbox, and per-step goai options.
	// P7.7.8: per-step maxRetries override via step.MaxRetries field.
	// Always create a per-step runner: StepID is step-specific and parallel
	// steps would race on the shared Runner.StepID field.
	stepGoAIOpts := e.Runner.goAIOptions
	if step.MaxRetries != nil {
		stepGoAIOpts = append(stepGoAIOpts[:len(stepGoAIOpts):len(stepGoAIOpts)], goai.WithMaxRetries(*step.MaxRetries))
	}
	// allocate a per-step *goai.AgentState and expose it
	// via Executor.AgentState(stepID) so the delivery-engine poller (R3)
	// can observe each step's tool-loop lifecycle.
	stepState := &goai.AgentState{}
	e.registerAgentState(stepID, stepState)
	// hold the step in ActiveSteps until the
	// 3-invariant termination rule (no senders + empty mailbox + stable
	// idle) holds. Without the wait, late coordinator messages targeted
	// at this step (e.g. arriving while pushStepEventToCoord runs after
	// the LLM call returns) would race the unregister and be dropped
	// silently. The wait runs first (LIFO defer order) so the engine
	// can still see ActiveSteps while we poll.
	defer e.unregisterAgentState(stepID)
	defer func() {
		if e.Router == nil || e.mailbox == nil {
			return
		}
 // Use a short polling interval so production termination is
 // fast for typical workflows; tests inject their own clock via
 // the function-direct path. The supplied tick uses time.Tick
 // (auto-stop on GC) so we don't need to manage ticker
 // lifetime here. F8 - apply HoldTimeout (or default 30s) to
 // cap the wait; on hold-timeout, drain remaining mailbox
 // messages and emit DropReasonHoldTimeout per message so the
 // "zero silent drops" contract holds even when the workflow
 // is hung waiting for senders that never close.
		hold := e.HoldTimeout
		if hold <= 0 {
			hold = defaultHoldTimeout
		}
 // Use NewTicker + defer Stop so the underlying timer goroutine is
 // reclaimed when this step's wait returns. Plain time.Tick (the
 // previous form) returns the channel but never stops the timer
 // - once-per-step leak that accumulates across long workflows.
 // The tickFunc closure ignores its argument because the ticker
 // is already constructed at the requested cadence. Wrapped in
 // an IIFE so the deferred Stop runs even if the wait panics
 // (the panic-clean exit was the latent leak missed).
		err := func() error {
			stepTicker := time.NewTicker(50 * time.Millisecond)
			defer stepTicker.Stop()
			return waitForStepTerminationWithHoldTimeout(
				ctx, stepID, e.Router, e.mailbox, stepState,
				func(time.Duration) <-chan time.Time { return stepTicker.C },
				50*time.Millisecond, hold, time.Now,
			)
		}()
		if IsHoldTimeout(err) {
			pending := e.mailbox.Unread(stepID)
			for _, msg := range pending {
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventMessageDropped,
						Timestamp: time.Now(),
						RunID:     runID,
						StepID:    stepID,
						Message:   fmt.Sprintf("[%s -> %s]: %s", msg.From, stepID, msg.Content),
						Data: map[string]any{
							"reason":   router.DropReasonHoldTimeout.String(),
							"from":     msg.From,
							"to":       stepID,
							"msg_type": int(msg.Type),
						},
					})
				}
			}
			if len(pending) > 0 {
				e.mailbox.MarkRead(stepID, MessageIDs(pending))
			}
		}
	}()
	// B1 fix: Router.Close MUST run BEFORE waitForStepTermination so that
	// late mailbox arrivals (e.g. a sibling step's pushStepEventToCoord
	// dispatching to *this* step after AgentRunner has already exited)
	// are drained as drop events instead of perpetually blocking the
	// wait. In Go's LIFO defer order, "runs before" means "deferred
	// AFTER" - hence this defer is below the wait defer above.
	// Sequence on natural step exit (LIFO): Router.Close drains pending
	// mailbox + emits target-terminal/workflow-cancelled drops + marks
	// closed → waitForStepTermination sees Unread==0 and stable idle,
	// returns immediately → unregisterAgentState removes from
	// ActiveSteps. Subsequent sibling Sends to this stepID hit the
	// closed flag in MessageRouter.Send and are emitted as
	// target-terminal drops via OnDrop.
	if e.Router != nil {
		defer e.Router.Close(stepID)
	}

	// when the mailbox stack is wired, allocate the
	// per-step Wake channel and register it with the engine. Pass both
	// into the AgentRunner so Run takes the mailbox-driven delivery
	// path. Mailbox+Wake are the sole delivery mechanism.
	var stepMailbox MailboxStore
	var stepWake chan struct{}
	if e.mailbox != nil && e.wakeRegistry != nil {
		stepMailbox = e.mailbox
		stepWake = make(chan struct{}, 1)
		e.wakeRegistry.Register(stepID, router.NewChanWakeTarget(stepWake))
		defer e.wakeRegistry.Unregister(stepID)
	}

	// + F7 - sender matrix.
	// In the current architecture sibling workflow steps NEVER call
	// Router.Send directly: only the coordinator's pushStepEventToCoord
	// path emits inter-step messages (and that path is per-step
	// scoped - it opens its own sender slot inside pushStepEventToCoord
	// before each Send). The DAG-aware rule (F7) therefore opens
	// just one slot per running step: the coordinator slot, held
	// until the step's full post-processing finishes (the
	// 3-invariant wait + unregister). The pre-F7 conservative
	// rule opened one slot per sibling step (NxN) which inflated
	// waitForStepTermination by ~100ms per step pair without
	// adding any safety - the slots covered Sends that simply
	// don't happen in the current routing topology.
	// SenderMatrixDAGAware controls the rule. Default is true; set
	// to false to retain the conservative NxN behavior.
	if e.Router != nil {
		if e.SenderMatrixDAGAware {
 // Single coordinator slot for this step.
			e.Router.OpenSender(stepID)
			defer e.Router.CloseSender(stepID)
		} else {
 // Pre-F7 conservative path - one slot per sibling.
			for _, other := range e.Workflow.Steps {
				if other.ID == stepID {
					continue
				}
				e.Router.OpenSender(other.ID)
			}
			defer func() {
				for _, other := range e.Workflow.Steps {
					if other.ID == stepID {
						continue
					}
					e.Router.CloseSender(other.ID)
				}
			}()
		}
	}

	stepRunner := &AgentRunner{
		model:         e.Runner.model,
		tools:         tools, // already includes base + shared memory tools
		goAIOptions:   stepGoAIOpts,
		permissions:   e.Runner.permissions,
		progress:      e.Runner.progress,
		streaming:     e.Runner.streaming,
		verbose:       e.Runner.verbose,
		runID:         e.Runner.runID,
		stepID:        stepID,
		stateRef:      stepState,
		mailbox:       stepMailbox,
		wake:          stepWake,
		maxWakeCycles: e.MaxWakeCycles,
		spawner:       e.Runner.spawner,
 // propagate the workflow Router into the per-step
 // AgentRunner so the Run loop's auto-inject hook (lines
 // 287-310 in agent_runner.go) can register the
 // `send_message` tool. Without this assignment the executor's
 // per-step runner has Router == nil and step LLMs never see
 // send_message - defeating the deliverable. Safe to assign
 // regardless of coordinator presence: when no coord runner was
 // installed via WithCoordinator, e.Router is nil and the
 // per-step runner's auto-inject hook short-circuits, matching
 // the "no coord = no send_message" invariant tested in
 // TestRunAgent_SendMessageToolInjected/router_nil_no_inject.
		router: e.Router,
 // transcript persistence. Only set when the
 // Run created a store (i.e. the mailbox+delivery stack is
 // active); non-mailbox Runs skip resume entirely.
		transcript: e.transcriptStore,
		modelID:    model,
 // : agent.Prompt is the agent identity (role), so it
 // lives in the system slot. prompt.go::AssemblePrompt no
 // longer prefixes "## Agent Role" into the user message - 
 // the system prompt + the per-step user instructions are
 // the canonical shape every LLM provider expects.
 // Verified end-to-end across the CLAUDE.md mandatory provider
 // matrix (gemini-3-pro-preview, bedrock anthropic.claude-sonnet-4-6,
 // bedrock minimax.minimax-m2.5, azure DeepSeek-V3.2, azure
 // claude-sonnet-4-6, azure gpt-5, azure gpt-5.3-codex).
		systemPrompt: agent.Prompt,
	}

	// VA-6: record the USER-SUPPLIED workflow model string for this
	// stepID so ResumeStep can match a saved transcript's Model (which
	// is this exact string) without forcing the caller to install a
	// ModelResolver. The wrapped provider.LanguageModel's ModelID
	// may differ (e.g., "us." cross-region prefix) so comparing against
	// that was default-broken.
	if model != "" {
		e.stepModelStringsMu.Lock()
		if e.stepModelStrings == nil {
			e.stepModelStrings = make(map[string]string)
		}
		e.stepModelStrings[stepID] = model
		e.stepModelStringsMu.Unlock()
	}

	stepStartEv := Event{
		Type:      types.EventStepStart,
		Timestamp: time.Now(),
		RunID:     runID,
		StepID:    stepID,
		AgentName: step.Agent,
		Data:      map[string]any{"index": index, "total": total},
	}
	if e.Progress != nil {
		e.Progress.OnEvent(ctx, stepStartEv)
	}
	e.pushCoordEvent(stepStartEv)

	stepStart := time.Now()

	// Apply step timeout: step-level > workflow default.
	stepCtx := ctx
	stepTimeout := step.Timeout.D()
	if stepTimeout <= 0 {
		stepTimeout = e.Workflow.Options.StepTimeout.D()
	}
	if stepTimeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, stepTimeout)
		defer cancel()
	}

	// Retry loop. Tokens are accumulated across all attempts (including failed ones).
	maxAttempts := step.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var agentResult *AgentResult
	var runErr error
	var retryTokens provider.Usage
	for range maxAttempts {
		agentResult, runErr = stepRunner.Run(stepCtx, agent, prompt, model, tools, attachments...)
		if agentResult != nil {
			addUsage(&retryTokens, agentResult.Tokens)
		}
		if runErr == nil {
			break
		}
		if stepCtx.Err() != nil {
			break
		}
	}

	stepDuration := time.Since(stepStart)

	// sr.ID = bare so the outer aggregator can look up
	// results by declared inner step ID. All other identity (events,
	// router, mailbox, runner) uses namespaced via the shadowed
	// stepID above.
	sr := &StepResult{ID: bareStepID, Duration: stepDuration}

	if runErr != nil {
		sr.Status = spec.StepFailed
		sr.Error = runErr
		sr.Tokens = retryTokens // include tokens from all attempts (including failed)
		stepTraceErr = runErr
		stepErrEv := Event{
			Type:      types.EventError,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			Error:     runErr,
			Duration:  stepDuration,
		}
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, stepErrEv)
		}
 // (Fix 11): the bare-lifecycle pushCoordEvent for
 // EventError was REMOVED here. Investigation found accidental
 // dual-push: the per-step post-`done` pushStepEventToCoord call
 // (at the Run-loop callsite) emits the same StepID + status with
 // richer counters (completed/failed/pending). Two coord-mailbox
 // messages per failed step were redundant noise; the post-done
 // push is the canonical one. StepStart still uses pushCoordEvent
 // (no equivalent post-done callsite exists for start events).
 // Invariant: TestExecutor_ExactlyOneStepEndPerStep.
		return sr
	}

	sr.Status = spec.StepCompleted
	sr.Content = agentResult.Content
	sr.Result = agentResult.Result
	sr.Tokens = retryTokens // accumulated across all attempts

	stepEndEv := Event{
		Type:      types.EventStepEnd,
		Timestamp: time.Now(),
		RunID:     runID,
		StepID:    stepID,
		AgentName: step.Agent,
		Duration:  stepDuration,
		Tokens:    &retryTokens, // accumulated across all attempts, consistent with sr.Tokens
	}
	if e.Progress != nil {
		e.Progress.OnEvent(ctx, stepEndEv)
	}
	// (Fix 11): the bare-lifecycle pushCoordEvent for
	// EventStepEnd was REMOVED here for the same reason as the EventError
	// case above - pushStepEventToCoord at the Run-loop callsite already
	// emits a richer step-end message with progress counters. Two coord-
	// mailbox messages per completed step were accidental redundancy.
	// StepStart still pushes via pushCoordEvent (no post-done equivalent).
	// Invariant: TestExecutor_ExactlyOneStepEndPerStep.

	return sr
}

// mergeResult merges extra keys into a copy of base, returning a new map.
// Neither base nor extra is mutated.
func mergeResult(base, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}
