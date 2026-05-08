package exec

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// ErrIncludePathEscape is returned (wrapped) when an include ref resolves
// to a path outside the workflow's BaseDir. Callers may match via
// errors.Is to distinguish path-traversal rejections from generic
// load-failures.
// Stable.
var ErrIncludePathEscape = errors.New("zenflow: include path escapes workflow directory")

// ErrIncludeDepthExceeded is returned (wrapped) when an include chain
// exceeds MaxIncludeDepth. Callers may match via errors.Is.
// Stable.
var ErrIncludeDepthExceeded = errors.New("zenflow: max include depth exceeded")

// runIncludeStep executes a step by loading and running a sub-workflow from the includes registry.
func (e *Executor) runIncludeStep(ctx context.Context, runID, stepID string, step Step, index, total int, depResults map[string]*StepResult) *StepResult {
	// Start include trace span.
	var stepTraceErr error
	if e.Tracer != nil {
		ctx = e.Tracer.StartSpan(ctx, "zenflow.include", map[string]string{
			"zenflow.step.id":     stepID,
			"zenflow.include.ref": step.Include,
		})
	}
	defer func() {
		if e.Tracer != nil {
			e.Tracer.EndSpan(ctx, stepTraceErr)
		}
	}()

	// G2: Recursive depth limit (spec §7 line 480).
	if e.IncludeDepth >= MaxIncludeDepth {
		stepTraceErr = fmt.Errorf("step %q include %q: depth %d: %w", stepID, step.Include, MaxIncludeDepth, ErrIncludeDepthExceeded)
		return &StepResult{ID: stepID, Status: spec.StepFailed, Error: stepTraceErr}
	}

	// Look up the include ref in Workflow.Includes, then fall back to direct file path.
	var fullPath string
	if e.Workflow.Includes != nil {
		if path, ok := e.Workflow.Includes[step.Include]; ok {
			fullPath = path
			if e.Workflow.BaseDir != "" && !filepath.IsAbs(path) {
				fullPath = filepath.Join(e.Workflow.BaseDir, path)
			}
		}
	}
	if fullPath == "" {
 // Not found in includes registry - treat as direct file path.
		fullPath = step.Include
		if e.Workflow.BaseDir != "" && !filepath.IsAbs(step.Include) {
			fullPath = filepath.Join(e.Workflow.BaseDir, step.Include)
		}
	}

	// Prevent path traversal: resolved path must stay within BaseDir.
	// When BaseDir is empty (programmatic workflows), reject absolute paths and ../ traversal.
	// Windows quirk: `filepath.IsAbs("/etc/passwd")` returns false because
	// Windows absolute paths require a drive letter (C:\...). A bare "/"
	// prefix is treated as drive-relative on Windows. For the security
	// check we explicitly reject leading-`/` paths in addition to native
	// absolute paths so a hostile workflow can't bypass the escape gate
	// just by running on Windows. Same goes for backslash on POSIX hosts -
	// a leading `\` is a relative path on POSIX but an absolute (current-
	// drive) path on Windows; reject it everywhere.
	if e.Workflow.BaseDir == "" {
		if filepath.IsAbs(fullPath) || strings.Contains(fullPath, "..") ||
			strings.HasPrefix(fullPath, "/") || strings.HasPrefix(fullPath, `\`) {
			stepTraceErr = fmt.Errorf("step %q include %q (no BaseDir set): %w", stepID, step.Include, ErrIncludePathEscape)
			return &StepResult{ID: stepID, Status: spec.StepFailed, Error: stepTraceErr}
		}
	} else {
 // Resolve symlinks before the prefix check to prevent symlink bypass.
		absResolved, _ := filepath.Abs(fullPath)
		if realResolved, err := filepath.EvalSymlinks(absResolved); err == nil {
			absResolved = realResolved
		}
		absBase, _ := filepath.Abs(e.Workflow.BaseDir)
		if realBase, err := filepath.EvalSymlinks(absBase); err == nil {
			absBase = realBase
		}
		if !strings.HasPrefix(absResolved, absBase+string(filepath.Separator)) && absResolved != absBase {
			stepTraceErr = fmt.Errorf("step %q include %q: %w", stepID, step.Include, ErrIncludePathEscape)
			return &StepResult{ID: stepID, Status: spec.StepFailed, Error: stepTraceErr}
		}
 // Use the resolved path to prevent TOCTOU race where a symlink could
 // be swapped between the security check and the actual file read.
		fullPath = absResolved
	}

	// Load the sub-workflow.
	subWF, err := LoadWorkflow(fullPath)
	if err != nil {
		stepTraceErr = fmt.Errorf("include %q: load sub-workflow: %w", step.Include, err)
		return &StepResult{ID: stepID, Status: spec.StepFailed, Error: stepTraceErr}
	}

	// G1: Agent name collision detection (spec §7 line 478).
	// Sub-workflow agents must not collide with parent workflow agents.
	if len(subWF.Agents) > 0 && len(e.Workflow.Agents) > 0 {
		collisions := make([]string, 0, len(subWF.Agents))
		for name := range subWF.Agents {
			if _, exists := e.Workflow.Agents[name]; exists {
				collisions = append(collisions, name)
			}
		}
		if len(collisions) > 0 {
			slices.Sort(collisions)
			quoted := make([]string, len(collisions))
			for i, name := range collisions {
				quoted[i] = fmt.Sprintf("%q", name)
			}
			stepTraceErr = fmt.Errorf("include %q: agent name collision with parent workflow: %s", step.Include, strings.Join(quoted, ", "))
			return &StepResult{ID: stepID, Status: spec.StepFailed, Error: stepTraceErr}
		}
	}

	// Apply parent step timeout to the sub-workflow.
	if step.Timeout.D() > 0 {
		subWF.Options.Timeout = step.Timeout
	}

	// Apply step timeout via context.
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

	stepStart := time.Now()

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventStepStart,
			Timestamp: time.Now(),
			RunID:     runID,
			StepID:    stepID,
			Data:      map[string]any{"index": index, "total": total, "include": step.Include},
		})
	}

	// G3: Inject parent dependency results into sub-workflow context.
	// Inner steps with no dependsOn receive parent dep results in their prompt
	// assembly (spec §7 dependsOn rewriting). ParentDepResults is checked in
	// the dispatch loop for steps with empty DependsOn.
	// - `maps.Clone` replaces the manual loop; cheaper and
	// clearer once the codebase is on Go 1.21+ (already required).
	var parentDeps map[string]*StepResult
	if len(depResults) > 0 {
		parentDeps = maps.Clone(depResults)
	}

	// G4: Namespace inner step IDs in progress events (spec §7 line 479).
	var nestedProgress ProgressSink
	if e.Progress != nil {
		nestedProgress = &nestedSuppressLifecycleSink{inner: e.Progress}
	}

	// propagate namespace + root router for include too.
	// Sub-workflow inner steps register delegations on root router.
	nestedPrefix := stepID
	if e.namespacePrefix != "" {
		nestedPrefix = e.namespacePrefix + "." + nestedPrefix
	}
	rootRouter := e.RootRouter
	if rootRouter == nil {
		rootRouter = e.Router
	}
	// Create nested executor for the sub-workflow.
	// Propagate all executor fields so sub-workflows have tracing, isolation, etc.
	nestedExec := &Executor{
		Runner:           e.Runner,
		Storage:          nil, // Inner results aggregated into parent - no orphan storage runs.
		Progress:         nestedProgress,
		Workflow:         subWF,
		DefaultModel:     e.DefaultModel,
		ForceModel:       e.ForceModel,
		MaxConcurrency:   e.MaxConcurrency,
		Tracer:           e.Tracer,
		Isolation:        e.Isolation,
		SharedMem:        e.SharedMem,
		Coordinator:      e.Coordinator,
		IncludeDepth:     e.IncludeDepth + 1,
		ParentDepResults: parentDeps,
		RootRouter:       rootRouter,
		namespacePrefix:  nestedPrefix,
	}

	// Retry loop for sub-workflow.
	// Validation guarantees Retries >= 0, so maxAttempts >= 1.
	maxAttempts := step.Retries + 1

	var subResult *WorkflowResult
	var runErr error
	var retryTokens provider.Usage
	for range maxAttempts {
		subResult, runErr = nestedExec.Run(stepCtx)
		if subResult != nil {
			addUsage(&retryTokens, subResult.Tokens)
		}
		if runErr == nil && subResult.Status == spec.StatusCompleted {
			break
		}
		if stepCtx.Err() != nil {
			break
		}
	}

	stepDuration := time.Since(stepStart)
	sr := &StepResult{ID: stepID, Duration: stepDuration}

	if runErr != nil {
		sr.Status = spec.StepFailed
		sr.Error = fmt.Errorf("include %q: %w", step.Include, runErr)
		sr.Tokens = retryTokens // include tokens from all attempts (including failed)
		stepTraceErr = sr.Error
		return sr
	}

	// Aggregate sub-workflow results with namespaced IDs ({parentStepID}.{innerStepID}).
	sr.Tokens = retryTokens // accumulated across all retry attempts
	innerSteps := make(map[string]any, len(subResult.Steps))
	if subResult.Status == spec.StatusCompleted {
		sr.Status = spec.StepCompleted
 // Build namespaced result map from all inner steps.
		for innerID, subSR := range subResult.Steps {
			namespacedID := stepID + "." + innerID
			innerSteps[namespacedID] = map[string]any{
				"content": subSR.Content,
				"status":  string(subSR.Status),
			}
		}
 // Use the topologically last completed step's content (deterministic).
 // Iterate sub-workflow steps in declaration order (stable) to find the last completed.
		for i := len(subWF.Steps) - 1; i >= 0; i-- {
			innerID := subWF.Steps[i].ID
			if subSR, ok := subResult.Steps[innerID]; ok && subSR.Status == spec.StepCompleted {
				sr.Content = subSR.Content
				sr.Result = subSR.Result
				break
			}
		}
		sr.Result = mergeResult(sr.Result, map[string]any{"innerSteps": innerSteps})
	} else {
		sr.Status = spec.StepFailed
		sr.Error = fmt.Errorf("include %q: sub-workflow status: %s", step.Include, subResult.Status)
		stepTraceErr = sr.Error
	}

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
			Duration:  stepDuration,
			Tokens:    &sr.Tokens,
			Error:     sr.Error,
		})
	}

	return sr
}
