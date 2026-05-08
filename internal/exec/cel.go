package exec

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

// cachedCELEnv returns a cached CEL environment, creating it once on first call.
// This avoids recreating the environment on every CEL evaluation, which is
// wasteful in tight loops (e.g., forEach with many items or repeat-until).
var cachedCELEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("steps", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("iteration", cel.IntType),
		cel.Variable("item", cel.DynType),
		cel.Variable("index", cel.IntType),
		cel.Variable("content", cel.StringType),
		cel.Variable("result", cel.DynType),
		cel.Variable("status", cel.StringType),
	)
})

// getCELEnv is the function used by EvaluateCEL/EvaluateCELToArray to obtain
// the CEL environment. Package-level var for test injection.
var getCELEnv = cachedCELEnv

// celProgramCache memoises compiled CEL programs by their expression
// string. CEL parse + type-check is non-trivial; without caching a
// repeat-until loop with maxIterations=50 would re-compile the same
// `until` expression 50 times. The cache is unbounded by design - the
// number of distinct CEL expressions in any one workflow is bounded
// by the YAML file (one per `if:`, `until:`, `forEach:`), so the
// cache cannot grow without operator action.
var celProgramCache sync.Map // expr string → *celCachedProgram

// celCachedProgram wraps the lazy compile result so the first caller
// performs the compile and subsequent callers see the same outcome.
type celCachedProgram struct {
	prog cel.Program
	err  error
}

// compileCEL returns a cached compiled CEL program for expr, compiling
// once on first use and reusing thereafter. Errors from invalid syntax
// are cached too so a bad expression surfaces consistently without
// re-compiling each call. getCELEnv is consulted on every call (NOT
// cached) so test stubs that inject env errors still surface even on
// expressions a previous test compiled successfully.
func compileCEL(expr string) (cel.Program, error) {
	env, envErr := getCELEnv()
	if envErr != nil {
		return nil, fmt.Errorf("cel env: %w", envErr)
	}
	if v, ok := celProgramCache.Load(expr); ok {
		c := v.(*celCachedProgram)
		return c.prog, c.err
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		c := &celCachedProgram{err: fmt.Errorf("cel compile %q: %w", expr, issues.Err())}
		celProgramCache.Store(expr, c)
		return nil, c.err
	}
	prog, progErr := env.Program(ast, cel.CostLimit(10000))
	c := &celCachedProgram{prog: prog, err: progErr}
	celProgramCache.Store(expr, c)
	return prog, progErr
}

// EvaluateCEL compiles and evaluates a CEL expression against the given context.
// The expression MUST evaluate to a bool; non-bool results return an error.
// Note: CEL evaluation does not accept a context.Context for cancellation.
// CostLimit(10000) bounds CPU cost. Expressions complete in microseconds-milliseconds.
func EvaluateCEL(expr string, ctx *EvalContext) (bool, error) {
	prog, err := compileCEL(expr)
	if err != nil {
		return false, err
	}

	// Build activation map from EvalContext.
	result := ctx.Result
	if result == nil {
		result = map[string]any{}
	}
	vars := map[string]any{
		"steps":     buildStepsMap(ctx.Steps),
		"iteration": ctx.Iteration,
		"item":      ctx.Item,
		"index":     ctx.Index,
		"content":   ctx.Content,
		"result":    result,
		"status":    ctx.Status,
	}

	out, _, err := prog.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("cel eval %q: %w", expr, err)
	}

	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("cel eval %q: result is %T, want bool", expr, out.Value())
	}
	return b, nil
}

// EvaluateCELToArray compiles and evaluates a CEL expression that must return a list.
// Used by forEach CEL expressions to produce the iteration array.
func EvaluateCELToArray(expr string, ctx *EvalContext) ([]any, error) {
	prog, err := compileCEL(expr)
	if err != nil {
		return nil, err
	}

	// Build activation map from EvalContext.
	result := ctx.Result
	if result == nil {
		result = map[string]any{}
	}
	vars := map[string]any{
		"steps":     buildStepsMap(ctx.Steps),
		"iteration": ctx.Iteration,
		"item":      ctx.Item,
		"index":     ctx.Index,
		"content":   ctx.Content,
		"result":    result,
		"status":    ctx.Status,
	}

	out, _, err := prog.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("cel eval %q: %w", expr, err)
	}

	// CEL returns native Go types for list expressions when inputs are native Go
	// values (map[string]any, []any). The []ref.Val path is unreachable in practice
	// because our activation map uses only native Go types - removed as dead code.
	if nativeSlice, ok := out.Value().([]any); ok {
		return nativeSlice, nil
	}

	return nil, fmt.Errorf("cel eval %q: result is %T, want list", expr, out.Value())
}

// buildStepsMap converts the EvalContext steps into a CEL-compatible map.
// Each step is a map with keys: content, result, status.
func buildStepsMap(steps map[string]*EvalStepContext) map[string]any {
	m := make(map[string]any, len(steps))
	for id, sc := range steps {
		if sc == nil {
			continue
		}
		stepMap := map[string]any{
			"content": sc.Content,
			"status":  sc.Status,
			"result":  sc.Result,
		}
		m[id] = stepMap
	}
	return m
}

// BuildEvalContext creates an EvalContext from the executor's results map.
// Must be called with the executor's mu lock held.
func BuildEvalContext(results map[string]*StepResult) *EvalContext {
	steps := make(map[string]*EvalStepContext, len(results))
	for id, sr := range results {
		if sr == nil {
			continue // in-flight step, skip
		}
		steps[id] = &EvalStepContext{
			Content: sr.Content,
			Result:  sr.Result,
			Status:  string(sr.Status),
		}
	}
	return &EvalContext{Steps: steps}
}
