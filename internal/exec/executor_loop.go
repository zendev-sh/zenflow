package exec

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// runLoopStep executes a step with loop semantics (maxIterations, untilAgent).
// Creates a zenflow.loop parent span and zenflow.loop.iteration child spans
// per iteration. The inner runStep call has its zenflow.step span suppressed
// to avoid duplicate spans.
func (e *Executor) runLoopStep(ctx context.Context, runID, stepID string, step Step, index, total int, depResults map[string]*StepResult) *StepResult {
	loop := step.Loop
	if loop == nil {
		return e.runStep(ctx, runID, stepID, step, index, total, depResults)
	}

	// forEach mode - delegate to runForEachStep.
	if loop.ForEach != nil {
		return e.runForEachStep(ctx, runID, stepID, step, index, total, depResults)
	}

	stepStart := time.Now()

	maxIter := 100 // safety cap
	if loop.MaxIterations != nil && *loop.MaxIterations > 0 {
		maxIter = *loop.MaxIterations
	}

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventStepStart,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			AgentName: step.Agent,
			Data:      map[string]any{"index": index, "total": total, "loop_type": "repeat"},
		})
	}

	// Start zenflow.loop parent span.
	if e.Tracer != nil {
		loopType := "repeat"
		if loop.UntilAgent != "" {
			loopType = "repeat-until"
		}
		ctx = e.Tracer.StartSpan(ctx, "zenflow.loop", map[string]string{
			"zenflow.step.id":   stepID,
			"zenflow.loop.type": loopType,
		})
	}

	var lastResult *StepResult
	var loopTokens provider.Usage // accumulate tokens across ALL iterations + judge calls
	var iterationHistory strings.Builder

	// Deferred token assignment guarantees ALL exit paths get accumulated tokens.
	// This eliminates the class of bug where a new return statement forgets to set tokens.
	// Also ends the zenflow.loop span and emits EventStepEnd/EventError.
	defer func() {
		if lastResult != nil {
 // Fix #3: outputMode=cumulative overrides Content with the full
 // iteration history so dependent aggregator steps see ALL rounds,
 // not just the final iteration's last inner step. Default ("last")
 // preserves the pre-fix behavior where downstream sees only the
 // final inner step content (right for refine-style loops where
 // each iteration supersedes the prior).
			if loop.OutputMode == spec.LoopOutputModeCumulative && iterationHistory.Len() > 0 {
				lastResult.Content = iterationHistory.String()
 // cumulative content is intentionally large - opt out
 // of writeDepSection's 16KB per-dep truncation so dependent
 // aggregator steps (verdict, summarizer) actually receive the
 // full history they were designed to consume. Without this
 // flag, the cumulative content gets cut at 16KB and the
 // dependent step sees `[truncated for context limit]`.
 // The overall 120KB prompt cap (maxPromptBytes) still applies.
				lastResult.PreserveContent = true
			}
			lastResult.Tokens = loopTokens
			lastResult.Duration = time.Since(stepStart)
		}
		if e.Tracer != nil {
			var loopErr error
			if lastResult != nil {
				loopErr = lastResult.Error
			}
			e.Tracer.EndSpan(ctx, loopErr)
		}
 // Emit EventStepEnd or EventError for the loop step.
		if e.Progress != nil && lastResult != nil {
			evType := types.EventStepEnd
			if lastResult.Status == spec.StepFailed {
				evType = types.EventError
			}
			e.Progress.OnEvent(ctx, Event{
				Type:      evType,
				Timestamp: time.Now(),
				RunID:     runID,
				StepID:    stepID,
				AgentName: step.Agent,
				Duration:  lastResult.Duration,
				Tokens:    &loopTokens,
				Error:     lastResult.Error,
			})
		}
	}()

	for iteration := range maxIter {
 // Apply delay between iterations (not before the first).
		if loop.Delay.D() > 0 && iteration > 0 {
			delayTimer := time.NewTimer(loop.Delay.D())
			select {
			case <-ctx.Done():
				delayTimer.Stop()
				lastResult = &StepResult{ID: stepID, Status: spec.StepFailed, Error: ctx.Err()}
				return lastResult // defer sets Tokens
			case <-delayTimer.C:
			}
		}

 // Start zenflow.loop.iteration span.
		iterCtx := ctx
		if e.Tracer != nil {
			iterCtx = e.Tracer.StartSpan(ctx, "zenflow.loop.iteration", map[string]string{
				"zenflow.step.id":        stepID,
				"zenflow.loop.iteration": strconv.Itoa(iteration),
			})
		}

 // Inject previous iteration output so the worker sees what was produced before.
		iterStep := step
		if iteration > 0 && lastResult != nil {
			iterStep.Instructions = fmt.Sprintf("## Previous Iteration Output (Iteration %d)\n%s\n\n%s",
				iteration, lastResult.Content, step.Instructions)
		}

 // Run the worker step (or inner DAG) with step-level tracing suppressed (loop manages spans).
		var innerDAGResults map[string]*StepResult // populated when loop has inner steps
		if len(loop.Steps) > 0 {
 // Inner DAG: build a mini-workflow from loop.steps and execute per iteration.
			lastResult, innerDAGResults = e.runRepeatUntilInnerDAG(iterCtx, runID, stepID, iterStep, loop.Steps, iteration, depResults)
		} else {
			stepCtx := withSkipStepTrace(iterCtx)
			lastResult = e.runStep(stepCtx, runID, stepID, iterStep, index, total, depResults)
		}
		addUsage(&loopTokens, lastResult.Tokens)
		if lastResult.Status != spec.StepCompleted {
			if e.Tracer != nil {
				e.Tracer.EndSpan(iterCtx, lastResult.Error)
			}
			return lastResult // defer sets Tokens
		}

 // Build cumulative iteration history for judge context. In cumulative
 // outputMode + inner-DAG, expand into per-inner-step subsections so
 // downstream aggregators (e.g. a verdict summarizer reading the loop
 // step's Content) see ALL inner steps' outputs, not just the last one.
		if loop.OutputMode == spec.LoopOutputModeCumulative && len(innerDAGResults) > 0 {
			fmt.Fprintf(&iterationHistory, "## Iteration %d\n", iteration+1)
			for _, innerStep := range loop.Steps {
				if sr, ok := innerDAGResults[innerStep.ID]; ok && sr != nil {
					fmt.Fprintf(&iterationHistory, "### %s\n%s\n\n", innerStep.ID, sr.Content)
				}
			}
		} else {
			fmt.Fprintf(&iterationHistory, "## Iteration %d\n%s\n\n", iteration+1, lastResult.Content)
		}

 // Evaluate CEL until expression after each iteration.
 // iteration is 0-based: first iteration = 0, second = 1, etc.
		if loop.Until != nil {
			evalCtx := BuildEvalContext(depResults)
 // Merge inner DAG step results into the CEL context so until
 // expressions can reference inner step outputs (e.g., steps.test.status).
			for id, sr := range innerDAGResults {
				evalCtx.Steps[id] = &EvalStepContext{
					Content: sr.Content,
					Result:  sr.Result,
					Status:  string(sr.Status),
				}
			}
			evalCtx.Iteration = iteration // 0-based per spec
			evalCtx.Content = lastResult.Content
			evalCtx.Status = string(lastResult.Status)
			if lastResult.Result != nil {
				evalCtx.Result = lastResult.Result
			}
 // Item and Index are not applicable in repeat-until loops
 // (only meaningful in forEach mode).
			done, err := EvaluateCEL(*loop.Until, evalCtx)
			if err != nil {
				lastResult.Status = spec.StepFailed
				lastResult.Error = fmt.Errorf("loop until eval (iteration %d): %w", iteration+1, err)
				if e.Tracer != nil {
					e.Tracer.EndSpan(iterCtx, lastResult.Error)
				}
				return lastResult // defer sets Tokens
			}
			if done {
				if e.Tracer != nil {
					e.Tracer.EndSpan(iterCtx, nil)
				}
				return lastResult // defer sets Tokens
			}
		}

 // If no untilAgent, just run maxIterations times.
		if loop.UntilAgent == "" {
			if e.Tracer != nil {
				e.Tracer.EndSpan(iterCtx, nil)
			}
			continue
		}

 // Run judge agent. Validation guarantees UntilAgent exists in Agents map
 // for flows through Run. Direct callers of runLoopStep may bypass validation.
		judgeAgent, ok := e.Workflow.Agents[loop.UntilAgent]
		if !ok {
			lastResult.Status = spec.StepFailed
			lastResult.Error = fmt.Errorf("run %q step %q: untilAgent %q not found in agents", runID, step.ID, loop.UntilAgent)
			if e.Tracer != nil {
				e.Tracer.EndSpan(iterCtx, lastResult.Error)
			}
			return lastResult // defer sets Tokens
		}

 // P7.7.11: Build judge prompt with explicit instructions + cumulative iteration history.
 // Without explicit instructions, judges frequently exhaust all turns without
 // calling submit_result or never return done:true, causing loop failures.
		var judgePromptBuf strings.Builder
		judgePromptBuf.WriteString("## Your Task: Evaluate Loop Progress\n\n")
		judgePromptBuf.WriteString("You are a judge evaluating whether the worker agent has completed its task satisfactorily.\n")
		judgePromptBuf.WriteString("Review the iteration history below and decide:\n")
		judgePromptBuf.WriteString("- If the task is DONE (quality is acceptable), call `submit_result` with `{\"done\": true}`\n")
		judgePromptBuf.WriteString("- If the task needs MORE WORK, call `submit_result` with `{\"done\": false}` and include feedback\n\n")
		judgePromptBuf.WriteString("IMPORTANT: You MUST call `submit_result` - do NOT just return text. ")
		judgePromptBuf.WriteString("If in doubt, lean toward `done: true` rather than looping indefinitely.\n\n")
		judgePromptBuf.WriteString(iterationHistory.String())
		if lastResult.Result != nil {
			resultJSON, _ := json.Marshal(lastResult.Result)
			fmt.Fprintf(&judgePromptBuf, "## Structured Result (Iteration %d)\n%s\n\n", iteration+1, string(resultJSON))
		}
		judgePrompt := judgePromptBuf.String()

 // Precedence (high → low): ForceModel (WithForceModel CLI override),
 // judgeAgent.Model, DefaultModel.
		judgeModel := cmp.Or(e.ForceModel, judgeAgent.Model, e.DefaultModel)

 // Resolve judge tools.
		judgeTools := FilterTools(e.Runner.tools, judgeAgent.Tools, judgeAgent.DisallowedTools)
 // submit_result is auto-injected by AgentRunner when ResultSchema is set.

 // Fix #1: clone e.Runner with a derived StepID so judge events carry
 // {stepID}.judge instead of the shared template runner's empty StepID.
 // Without this, stdout sink renders judge AgentTurn / ToolCall events
 // with empty brackets ("[] Thinking...", "[] submit_result"), making
 // it impossible to attribute judge activity when multiple loop:untilAgent
 // steps run in parallel. Per-step runner construction in runStep
 // follows the same pattern. Mutex/atomic finalize state on the original
 // runner is intentionally NOT carried - judge gets fresh zero-value
 // state and never participates in coord finalize signaling.
		judgeRunner := &AgentRunner{
			model:        e.Runner.model,
			tools:        e.Runner.tools,
			permissions:  e.Runner.permissions,
			progress:     e.Runner.progress,
			goAIOptions:  e.Runner.goAIOptions,
			streaming:    e.Runner.streaming,
			verbose:      e.Runner.verbose,
			runID:        e.Runner.runID,
			stepID:       stepID + ".judge",
			router:       e.Runner.router,
			modelID:      judgeModel,
			systemPrompt: judgeAgent.Prompt,
			spawner:      e.Runner.spawner,
		}
		judgeResult, err := judgeRunner.Run(ctx, judgeAgent, judgePrompt, judgeModel, judgeTools)

 // Accumulate judge tokens even on failure (partial results may carry token data).
		if judgeResult != nil {
			addUsage(&loopTokens, judgeResult.Tokens)
		}

		if err != nil {
 // Judge failure - fail-open by design. The loop continues bounded by maxIterations.
			if e.Progress != nil {
				e.Progress.OnEvent(ctx, Event{
					Type:      types.EventError,
					RunID:     runID,
					StepID:    stepID,
					Error:     fmt.Errorf("untilAgent judge failed (iteration %d): %w", iteration+1, err),
					Timestamp: time.Now(),
				})
			}
			if e.Tracer != nil {
				e.Tracer.EndSpan(iterCtx, err)
			}
			continue
		}

 // Append judge feedback to iteration history so the next work agent
 // iteration sees the judge's content as context (per spec).
		if judgeResult.Content != "" {
			fmt.Fprintf(&iterationHistory, "## Judge Feedback (Iteration %d)\n%s\n\n", iteration+1, judgeResult.Content)
		}

 // Check result.done.
		if judgeResult.Result != nil {
			if done, ok := judgeResult.Result["done"].(bool); ok && done {
				if e.Tracer != nil {
					e.Tracer.EndSpan(iterCtx, nil)
				}
				return lastResult // defer sets Tokens
			}
		}

 // End iteration span (judge did not say done, continue looping).
		if e.Tracer != nil {
			e.Tracer.EndSpan(iterCtx, nil)
		}
	}

	// If until/untilAgent was configured but never triggered, mark as failed.
	// When both are set, produce a combined error message.
	if (loop.Until != nil || loop.UntilAgent != "") && lastResult != nil {
		parts := make([]string, 0, 2)
		if loop.Until != nil {
			parts = append(parts, "until condition never became true")
		}
		if loop.UntilAgent != "" {
			parts = append(parts, "judge agent never returned done")
		}
		lastResult.Status = spec.StepFailed
		lastResult.Error = fmt.Errorf("run %q step %q: loop exhausted %d iterations: %s", runID, step.ID, maxIter, strings.Join(parts, " and "))
	}
	return lastResult // defer sets Tokens
}

// runForEachStep executes a step with forEach loop semantics.
// For static arrays, iterates over items (optionally in parallel with maxConcurrency).
// For CEL expressions (string), evaluates the expression to produce an array.
func (e *Executor) runForEachStep(ctx context.Context, runID, stepID string, step Step, index, total int, depResults map[string]*StepResult) *StepResult {
	loop := step.Loop

	// Resolve the forEach array. Validation guarantees ForEach is []any or string
	// and that static arrays are non-empty.
	var items []any
	switch v := loop.ForEach.(type) {
	case []any:
		items = v
	case string:
 // CEL expression - evaluate to produce array.
		resolved, err := e.evaluateForEachCEL(v, stepID, depResults)
		if err != nil {
			return &StepResult{ID: stepID, Status: spec.StepFailed, Error: fmt.Errorf("forEach cel eval: %w", err)}
		}
		items = resolved
	}

	// Delay is not applied in forEach mode (parallel iterations).

	stepStart := time.Now()

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventStepStart,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			AgentName: step.Agent,
			Data:      map[string]any{"index": index, "total": total, "loop_type": "forEach", "items": len(items)},
		})
	}

	// Start zenflow.loop parent span for forEach.
	if e.Tracer != nil {
		ctx = e.Tracer.StartSpan(ctx, "zenflow.loop", map[string]string{
			"zenflow.step.id":   stepID,
			"zenflow.loop.type": "forEach",
		})
	}
	var stepTraceErr error
	var spanStarted bool
	defer func() {
		if e.Tracer != nil && spanStarted {
			e.Tracer.EndSpan(ctx, stepTraceErr)
		}
	}()
	spanStarted = true

	// Determine concurrency: spec says default is all-parallel.
	maxConc := loop.MaxConcurrency
	if maxConc <= 0 {
		maxConc = len(items) // All-parallel by default (per spec).
	}
	// Cap goroutine count to prevent unbounded spawning.
	if maxConc > forEachMaxConcurrency {
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventMessage,
				Timestamp: time.Now(),
				RunID:     runID,
				StepID:    stepID,
				Message:   fmt.Sprintf("forEach maxConcurrency capped at %d (requested: %d)", forEachMaxConcurrency, maxConc),
			})
		}
		maxConc = forEachMaxConcurrency
	}

	type iterResult struct {
		index  int
		result *StepResult
	}

	results := make([]iterResult, len(items))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for i, item := range items {
		i, item := i, item
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					var iterErr error
					if e2, ok := r.(error); ok {
						iterErr = fmt.Errorf("panic in forEach iteration %d: %w", i, e2)
					} else {
						iterErr = fmt.Errorf("panic in forEach iteration %d: %v", i, r)
					}
					mu.Lock()
					results[i] = iterResult{index: i, result: &StepResult{ID: stepID, Status: spec.StepFailed, Error: iterErr}}
					errs = append(errs, iterErr)
					mu.Unlock()
				}
			}()
 // ctx here is abortCtx from the dispatch goroutine (passed through
 // runLoopStep -> runForEachStep), so ctx.Done correctly respects abort.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = iterResult{index: i, result: &StepResult{ID: stepID, Status: spec.StepCancelled, Error: ctx.Err()}}
				return
			}
			defer func() { <-sem }()

 // Check if a previous iteration already failed.
			mu.Lock()
			hasErr := len(errs) > 0
			mu.Unlock()
			if hasErr {
				results[i] = iterResult{index: i, result: &StepResult{ID: stepID, Status: spec.StepCancelled}}
				return
			}

 // Start per-iteration trace span.
			iterCtx := ctx
			if e.Tracer != nil {
				iterCtx = e.Tracer.StartSpan(ctx, "zenflow.loop.iteration", map[string]string{
					"zenflow.step.id":        stepID,
					"zenflow.loop.iteration": strconv.Itoa(i),
				})
			}

			iterStepID := fmt.Sprintf("%s[%d]", stepID, i)
			var sr *StepResult
			if len(loop.Steps) > 0 {
 // Inner DAG: build a mini-workflow from loop.steps and execute.
				sr = e.runForEachInnerDAG(iterCtx, runID, stepID, step, loop.Steps, item, i, depResults)
			} else {
 // Re-run parent step with forEach item injected into prompt via context.
 // Uses runStep so forEach iterations inherit isolation, tracing, SharedMem,
 // coordinator inbox, progress events, and storage persistence.
				feCtx := withForEachCtx(withSkipStepTrace(iterCtx), &ForEachContext{Item: item, Index: i})
				sr = e.runStep(feCtx, runID, iterStepID, step, index, total, depResults)
				sr.ID = stepID // Restore original step ID for result aggregation.
			}

 // End per-iteration trace span.
			if e.Tracer != nil {
				e.Tracer.EndSpan(iterCtx, sr.Error)
			}

			results[i] = iterResult{index: i, result: sr}

			if sr.Status == spec.StepFailed {
				mu.Lock()
				errs = append(errs, sr.Error)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	// Aggregate results.
	var totalTokens provider.Usage
	iterations := make([]any, len(items))
	var lastContent string
	var failed bool

	for _, ir := range results {
		sr := ir.result // guaranteed non-nil: all goroutines completed (wg.Wait) + panic recovery
		addUsage(&totalTokens, sr.Tokens)

		if sr.Status == spec.StepCompleted {
			lastContent = sr.Content
		}
		iterations[ir.index] = map[string]any{
			"id":      fmt.Sprintf("%s[%d]", stepID, ir.index),
			"index":   ir.index,
			"item":    items[ir.index],
			"content": sr.Content,
			"status":  string(sr.Status),
		}
		if sr.Status == spec.StepFailed || sr.Status == spec.StepCancelled {
			failed = true
		}
	}

	sr := &StepResult{
		ID:       stepID,
		Content:  lastContent,
		Result:   map[string]any{"iterations": iterations},
		Tokens:   totalTokens,
		Duration: time.Since(stepStart),
	}

	if failed {
		sr.Status = spec.StepFailed
		sr.Error = errors.Join(errs...) // nil if only cancellations, joined errors if any iteration failed
		stepTraceErr = sr.Error
	} else {
		sr.Status = spec.StepCompleted
	}

	// Emit EventStepEnd or EventError for the forEach step.
	if e.Progress != nil {
		evType := types.EventStepEnd
		if sr.Status == spec.StepFailed {
			evType = types.EventError
		}
		e.Progress.OnEvent(ctx, Event{
			Type:      evType,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			AgentName: step.Agent,
			Duration:  sr.Duration,
			Tokens:    &totalTokens,
			Error:     sr.Error,
		})
	}

	return sr
}

// evaluateForEachCEL evaluates a CEL expression to produce a forEach array.
// Uses full CEL evaluation with EvalContext built from dependency results.
func (e *Executor) evaluateForEachCEL(expr, stepID string, depResults map[string]*StepResult) ([]any, error) {
	ctx := BuildEvalContext(depResults)
	// Set top-level vars from the last completed dependency (sorted by key for determinism).
	// This allows CEL expressions like `content` or `result.items` in forEach context.
	depKeys := slices.Sorted(maps.Keys(depResults))
	for _, k := range depKeys {
		dep := depResults[k]
		if dep != nil && dep.Status == spec.StepCompleted {
			ctx.Content = dep.Content
			ctx.Result = dep.Result
			ctx.Status = string(dep.Status)
		}
	}
	return EvaluateCELToArray(expr, ctx)
}

// runRepeatUntilInnerDAG runs a mini-workflow from inner steps for a single repeat-until iteration.
// Similar to runForEachInnerDAG but without forEach item injection.
func (e *Executor) runRepeatUntilInnerDAG(ctx context.Context, runID, parentStepID string, parentStep Step, innerSteps []Step, iteration int, depResults map[string]*StepResult) (*StepResult, map[string]*StepResult) {
	steps := make([]Step, len(innerSteps))
	copy(steps, innerSteps)

	// Prepend iteration header to the first inner step's Instructions.
	// This is intentional: inner DAG steps run through the nested Executor which
	// calls runStep -> AssemblePrompt normally. The header is prepended BEFORE
	// the step reaches the nested executor, so AssemblePrompt will include it
	// along with Agent Role, Context Files, etc.
	iterHeader := fmt.Sprintf("## Iteration %d\n\n", iteration+1)
	if len(steps) > 0 {
		steps[0].Instructions = iterHeader + steps[0].Instructions
	}

	miniWF := &Workflow{
		Name:    fmt.Sprintf("%s-repeat-%d", parentStepID, iteration),
		Agents:  e.Workflow.Agents,
		Steps:   steps,
		BaseDir: e.Workflow.BaseDir,
		Options: WorkflowOptions{
			MaxConcurrency: e.Workflow.Options.MaxConcurrency,
			OnStepFailure:  spec.FailureCascade,
			StepTimeout:    e.Workflow.Options.StepTimeout, // propagate parent workflow's default step timeout
		},
	}

	if parentStep.Timeout.D() > 0 {
		miniWF.Options.Timeout = parentStep.Timeout
	}

	// Wrap progress sink to namespace inner step IDs as parentStepID.N.stepID.
	var nestedProgress ProgressSink
	if e.Progress != nil {
		nestedProgress = &nestedSuppressLifecycleSink{inner: e.Progress}
	}
	// propagate the namespace prefix and root router into the
	// nested executor. The prefix scopes inner step IDs (mailbox keys,
	// runner.StepID, push events, send_message From) so all coord-side
	// observers see consistent namespaced names. The root router is the
	// OUTERMOST executor's Router; nested executors register
	// delegations there so coord (which always sends via root) can
	// reach inner steps.
	nestedPrefix := parentStepID + "." + strconv.Itoa(iteration)
	if e.namespacePrefix != "" {
		nestedPrefix = e.namespacePrefix + "." + nestedPrefix
	}
	rootRouter := e.RootRouter
	if rootRouter == nil {
		rootRouter = e.Router
	}
	nestedExec := &Executor{
		Runner:          e.Runner,
		Storage:         nil, // Inner DAG results aggregated into parent - no orphan storage runs.
		Progress:        nestedProgress,
		Workflow:        miniWF,
		DefaultModel:    e.DefaultModel,
		ForceModel:      e.ForceModel,
		MaxConcurrency:  e.MaxConcurrency,
		Tracer:          e.Tracer,
		Isolation:       e.Isolation,
		SharedMem:       e.SharedMem,
		Coordinator:     e.Coordinator,
		RootRouter:      rootRouter,
		namespacePrefix: nestedPrefix,
	}

	result, err := nestedExec.Run(ctx)
	if err != nil {
		return &StepResult{ID: parentStepID, Status: spec.StepFailed, Error: fmt.Errorf("run %q step %q: repeat-until inner DAG (iteration %d): %w", runID, parentStepID, iteration, err)}, nil
	}

	var totalTokens provider.Usage
	var lastContent string
	var lastResult map[string]any
	// iterate innerSteps in DECLARATION ORDER (slice), not the
	// random map order of result.Steps. Map iteration in Go is randomized,
	// so the previous "for _, sr := range result.Steps" non-deterministically
	// picked any completed step's Content as the loop step's Content. For
	// `outputMode: last` (default), the contract is "the LAST inner step's
	// content survives" - which is the LAST item in loop.Steps declaration
	// order. The flake surfaced as
	// TestExecutor_LoopStep_OutputMode_Last_DefaultBackwardCompat
	// returning "PRO_R1" instead of "CON_R1" under -count=20 stress.
	// Token accumulation still iterates every step's result regardless
	// of order (sum is order-independent).
	for _, sr := range result.Steps {
		if sr != nil {
			addUsage(&totalTokens, sr.Tokens)
		}
	}
	for _, innerStep := range innerSteps {
		sr := result.Steps[innerStep.ID]
		if sr == nil || sr.Status != spec.StepCompleted {
			continue
		}
		lastContent = sr.Content
		lastResult = sr.Result
	}

	sr := &StepResult{
		ID:      parentStepID,
		Content: lastContent,
		Result:  lastResult,
		Tokens:  totalTokens,
	}
	if result.Status == spec.StatusCompleted {
		sr.Status = spec.StepCompleted
	} else {
		sr.Status = spec.StepFailed
		sr.Error = fmt.Errorf("run %q step %q: repeat-until inner DAG (iteration %d) status: %s", runID, parentStepID, iteration, result.Status)
 // sr.Content and sr.Result are already set from lastContent/lastResult above,
 // preserving partial content from completed inner steps even on failure.
	}
	return sr, result.Steps
}

// runForEachInnerDAG runs a mini-workflow from inner steps for a single forEach item.
func (e *Executor) runForEachInnerDAG(ctx context.Context, runID, parentStepID string, parentStep Step, innerSteps []Step, item any, itemIndex int, depResults map[string]*StepResult) *StepResult {
	// Copy inner steps and inject forEach item context into the first step's instructions.
	// This ensures inner DAG steps know which forEach item they are processing.
	steps := make([]Step, len(innerSteps))
	copy(steps, innerSteps)

	itemJSON, jsonErr := json.Marshal(item)
	var itemStr string
	if jsonErr != nil {
		itemStr = fmt.Sprintf("%v", item)
	} else {
		itemStr = string(itemJSON)
	}
	// Prepend forEach item header to the first inner step's Instructions.
	// This is intentional: inner DAG steps run through the nested Executor which
	// calls runStep -> AssemblePrompt normally. The header is prepended BEFORE
	// the step reaches the nested executor, so AssemblePrompt will include it
	// along with Agent Role, Context Files, etc.
	forEachHeader := fmt.Sprintf("## forEach Item (index: %d)\n%s\n\n", itemIndex, itemStr)
	if len(steps) > 0 {
		steps[0].Instructions = forEachHeader + steps[0].Instructions
	}

	// Build a mini-workflow from the inner steps.
	// Inner steps inherit agents from the parent workflow.
	miniWF := &Workflow{
		Name:    fmt.Sprintf("%s-foreach-%d", parentStepID, itemIndex),
		Agents:  e.Workflow.Agents,
		Steps:   steps,
		BaseDir: e.Workflow.BaseDir,
		Options: WorkflowOptions{
			MaxConcurrency: e.Workflow.Options.MaxConcurrency,
			OnStepFailure:  spec.FailureCascade,
			StepTimeout:    e.Workflow.Options.StepTimeout, // propagate parent workflow's default step timeout
		},
	}

	// Apply parent step timeout to the sub-workflow.
	if parentStep.Timeout.D() > 0 {
		miniWF.Options.Timeout = parentStep.Timeout
	}

	// Create a nested executor with the same Runner and tools.
	// Propagate all executor fields so sub-workflows have tracing, isolation, etc.
	// Wrap progress sink to namespace inner step IDs as parentStepID.N.stepID.
	var nestedProgress ProgressSink
	if e.Progress != nil {
		nestedProgress = &nestedSuppressLifecycleSink{inner: e.Progress}
	}
	// same as repeat-until: propagate namespace + root router
	// so forEach iterations' inner steps are reachable from coord via
	// the root router's delegation map. forEach uses bracketed
	// notation `parent[N]` for the index segment to match the
	// established convention (matches namespacedProgressSink prefix
	// above).
	nestedPrefix := fmt.Sprintf("%s[%d]", parentStepID, itemIndex)
	if e.namespacePrefix != "" {
		nestedPrefix = e.namespacePrefix + "." + nestedPrefix
	}
	rootRouter := e.RootRouter
	if rootRouter == nil {
		rootRouter = e.Router
	}
	nestedExec := &Executor{
		Runner:          e.Runner,
		Storage:         nil, // Inner DAG results aggregated into parent - no orphan storage runs.
		Progress:        nestedProgress,
		Workflow:        miniWF,
		DefaultModel:    e.DefaultModel,
		ForceModel:      e.ForceModel,
		MaxConcurrency:  e.MaxConcurrency,
		Tracer:          e.Tracer,
		Isolation:       e.Isolation,
		SharedMem:       e.SharedMem,
		Coordinator:     e.Coordinator,
		RootRouter:      rootRouter,
		namespacePrefix: nestedPrefix,
	}

	result, err := nestedExec.Run(ctx)
	if err != nil {
		return &StepResult{ID: parentStepID, Status: spec.StepFailed, Error: fmt.Errorf("run %q step %q: forEach inner DAG (item %d): %w", runID, parentStepID, itemIndex, err)}
	}

	// Aggregate inner DAG results into a single StepResult.
	var totalTokens provider.Usage
	var lastContent string
	// iterate innerSteps in DECLARATION ORDER for content
	// extraction; map order is randomized in Go and would
	// non-deterministically pick any completed step. Token sum is
	// order-independent so the loop over result.Steps is fine for
	// that.
	for _, sr := range result.Steps {
		if sr != nil {
			addUsage(&totalTokens, sr.Tokens)
		}
	}
	for _, innerStep := range innerSteps {
		sr := result.Steps[innerStep.ID]
		if sr == nil || sr.Status != spec.StepCompleted {
			continue
		}
		lastContent = sr.Content
	}

	sr := &StepResult{
		ID:      parentStepID,
		Content: lastContent,
		Tokens:  totalTokens,
	}
	if result.Status == spec.StatusCompleted {
		sr.Status = spec.StepCompleted
	} else {
		sr.Status = spec.StepFailed
		sr.Content = lastContent // preserve partial content from completed inner steps
		sr.Error = fmt.Errorf("run %q step %q: forEach inner DAG (item %d) status: %s", runID, parentStepID, itemIndex, result.Status)
	}
	return sr
}
