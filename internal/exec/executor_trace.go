package exec

import "context"

// skipStepTraceKey is a context key that suppresses the zenflow.step span
// inside runStep. Used by runLoopStep to replace zenflow.step with
// zenflow.loop.iteration spans.
type skipStepTraceKey struct{}

// forEachCtxKey is a context key that carries forEach iteration context into runStep.
// When set, runStep calls AssemblePromptWithForEach instead of AssemblePrompt.
type forEachCtxKey struct{}

// withSkipStepTrace returns a context that tells runStep to skip its own
// zenflow.step span creation. Used when the caller (e.g., runLoopStep)
// manages its own span hierarchy.
func withSkipStepTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipStepTraceKey{}, true)
}

// shouldSkipStepTrace returns true if the context carries the skip-step-trace flag.
func shouldSkipStepTrace(ctx context.Context) bool {
	v, _ := ctx.Value(skipStepTraceKey{}).(bool)
	return v
}

// withForEachCtx returns a context carrying the forEach iteration context.
// runStep checks for this and uses AssemblePromptWithForEach when present.
func withForEachCtx(ctx context.Context, fe *ForEachContext) context.Context {
	return context.WithValue(ctx, forEachCtxKey{}, fe)
}

// getForEachCtx returns the forEach context from ctx, or nil if not set.
func getForEachCtx(ctx context.Context) *ForEachContext {
	fe, _ := ctx.Value(forEachCtxKey{}).(*ForEachContext)
	return fe
}
