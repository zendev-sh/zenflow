package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// runToolStep executes a step whose Tool field is set, bypassing LLM execution.
// It looks up the named tool in e.Tools, resolves ToolInput values (evaluating
// string values starting with '$' as CEL expressions), marshals the resolved
// map to JSON, and calls tool.Execute.
func (e *Executor) runToolStep(ctx context.Context, runID, stepID string, step Step, depResults map[string]*StepResult, index, total int) *StepResult {
	stepStart := time.Now()

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventStepStart,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			Data:      map[string]any{"index": index, "total": total},
		})
	}

	// 1. Find the tool by name.
	var found bool
	var toolExec func(context.Context, json.RawMessage) (string, error)
	for _, t := range e.Tools {
		if t.Name == step.Tool {
			toolExec = t.Execute
			found = true
			break
		}
	}
	if !found {
		runErr := fmt.Errorf("tool step %q: tool %q not found in registered tools", stepID, step.Tool)
		duration := time.Since(stepStart)
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventError,
				Timestamp: time.Now(),
				RunID:     runID,
				StepID:    stepID,
				Error:     runErr,
				Duration:  duration,
			})
		}
		return &StepResult{ID: stepID, Status: spec.StepFailed, Error: runErr, Duration: duration}
	}

	// 2. Build EvalContext from completed dep results.
	evalCtx := BuildEvalContext(depResults)

	// 3. Resolve ToolInput: string values starting with '$' are CEL expressions.
	resolved := make(map[string]any, len(step.ToolInput))
	for k, v := range step.ToolInput {
		sv, ok := v.(string)
		switch {
		case ok && strings.HasPrefix(sv, "$$"):
			// Escaped literal: a leading "$$" collapses to a single "$"
			// and is passed through verbatim (no CEL evaluation). This is
			// how a value that genuinely starts with "$" (e.g. "$HOME",
			// "$5.00") is expressed.
			resolved[k] = sv[1:]
		case ok && strings.HasPrefix(sv, "$"):
			celResult, err := EvaluateCELToString(sv[1:], evalCtx)
			if err != nil {
				runErr := fmt.Errorf("tool step %q: CEL input %q: %w", stepID, k, err)
				duration := time.Since(stepStart)
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventError,
						Timestamp: time.Now(),
						RunID:     runID,
						StepID:    stepID,
						Error:     runErr,
						Duration:  duration,
					})
				}
				return &StepResult{ID: stepID, Status: spec.StepFailed, Error: runErr, Duration: duration}
			}
			resolved[k] = celResult
		default:
			resolved[k] = v
		}
	}

	// 4. Marshal resolved map to json.RawMessage.
	jsonInput, err := json.Marshal(resolved)
	if err != nil {
		runErr := fmt.Errorf("tool step %q: marshal tool input: %w", stepID, err)
		duration := time.Since(stepStart)
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventError,
				Timestamp: time.Now(),
				RunID:     runID,
				StepID:    stepID,
				Error:     runErr,
				Duration:  duration,
			})
		}
		return &StepResult{ID: stepID, Status: spec.StepFailed, Error: runErr, Duration: duration}
	}

	// 5. Apply step timeout: step-level > workflow default.
	toolCtx := ctx
	stepTimeout := step.Timeout.D()
	if stepTimeout <= 0 {
		stepTimeout = e.Workflow.Options.StepTimeout.D()
	}
	if stepTimeout > 0 {
		var cancel context.CancelFunc
		toolCtx, cancel = context.WithTimeout(ctx, stepTimeout)
		defer cancel()
	}

	// 6. Call tool.Execute.
	result, err := toolExec(toolCtx, json.RawMessage(jsonInput))
	duration := time.Since(stepStart)
	if err != nil {
		runErr := fmt.Errorf("tool step %q: %w", stepID, err)
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventError,
				Timestamp: time.Now(),
				RunID:     runID,
				StepID:    stepID,
				Error:     runErr,
				Duration:  duration,
			})
		}
		return &StepResult{ID: stepID, Status: spec.StepFailed, Error: runErr, Duration: duration}
	}

	// 7. Success.
	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventStepEnd,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			Duration:  duration,
		})
	}

	return &StepResult{
		ID:       stepID,
		Status:   spec.StepCompleted,
		Content:  result,
		Duration: duration,
	}
}
