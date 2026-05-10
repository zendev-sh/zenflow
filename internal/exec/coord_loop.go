package exec

import (
	"context"
	"log/slog"
	"time"
)

// DefaultCoordCleanupTimeout is the default wall-clock cap on the
// cleanup phase of RunCoordinatorLoop: the cleanup func cancels the
// background coord ctx, then waits up to this duration for the
// goroutine to acknowledge the cancel before returning. A hung coord
// LLM that ignores ctx cancellation will leak its goroutine until
// process exit rather than block CLI shutdown indefinitely.
const DefaultCoordCleanupTimeout = 2 * time.Second

// coordLoopConfig collects internal CoordLoopOption settings.
type coordLoopConfig struct {
	cleanupTimeout time.Duration
}

// CoordLoopOption configures RunCoordinatorLoop.
// Stable.
type CoordLoopOption func(*coordLoopConfig)

// WithCleanupTimeout overrides the default cleanup wall-clock cap
// (DefaultCoordCleanupTimeout, 2s) used by the cleanup func returned
// from RunCoordinatorLoop. Useful in tests that want a sub-second cap
// to exercise the timer-fired branch quickly. Zero or negative values
// fall back to DefaultCoordCleanupTimeout.
// Stable.
func WithCleanupTimeout(d time.Duration) CoordLoopOption {
	return func(c *coordLoopConfig) {
		if d > 0 {
			c.cleanupTimeout = d
		}
	}
}

// RunCoordinatorLoop spawns the coordinator AgentRunner's Run loop on
// a background goroutine and returns a cleanup func the caller must
// defer. Suitable for CLI / TUI / embedder consumers that own a
// coordinator runner across the lifetime of a workflow run.
// The loop:
// - Calls runner.Run with DefaultCoordColdStartPrompt + the current
// BuildCoordStepMenu(runner) snapshot on cold start.
// - On natural-stop+empty-mailbox exit, blocks in WaitForCoordWake
// until either the mailbox has pending events or runner.Wake fires.
// - On wake, re-spawns runner.Run with DefaultCoordContinuationPrompt
// + a refreshed step menu so the LLM sees current state.
// - Logs LLM errors via slog.WarnContext (non-fatal - the workflow
// DAG continues even when coord narration fails).
// - Exits when ctx is cancelled.
// IGNORE coord's finalize call: workflow lifetime is owned by the
// executor, not the coord LLM. Sonnet (observed) hallucinates workflow
// state from aggregated counters in RouterMessage content and finalizes
// after the FIRST step despite prompt warnings. Coord lifetime is
// bound to ctx cancellation; the finalize tool remains callable for
// downstream consumers (TUI integration) that may want the signal.
// When runner is nil, returns a no-op cleanup func.
// Stable.
func RunCoordinatorLoop(ctx context.Context, runner *AgentRunner, modelID string, opts ...CoordLoopOption) func() {
	if runner == nil {
		return func() {}
	}
	cfg := coordLoopConfig{cleanupTimeout: DefaultCoordCleanupTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	coordCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(coordCtx, "coordinator goroutine panicked", "panic", r)
			}
		}()
		defer close(done)
		// AgentRunner.Run exits on natural-stop+empty-mailbox - that
		// exit is correct for STEP runners (the executor re-spawns Run
		// per step) but wrong for COORD, which lives across the whole
		// workflow. Re-spawn loop until ctx canceled.
		coordCfg := AgentConfig{}
		userMsg := DefaultCoordColdStartPrompt + BuildCoordStepMenu(runner)
		for {
			_, runErr := runner.Run(coordCtx, coordCfg, userMsg, modelID, runner.Tools())
			// Surface coord LLM errors so users see rate-limit / network
			// failures instead of silent absence-of-narration. The error
			// is non-fatal (the workflow DAG continues independently of
			// the coordinator) but operators need to know when the coord
			// goroutine is failing. Skip ctx-cancel errors - those are
			// the normal teardown path and don't indicate a problem.
			if runErr != nil && coordCtx.Err() == nil {
				slog.WarnContext(coordCtx, "coordinator runner failed; workflow DAG continues without coord narration",
					"err", runErr,
					"model", modelID,
				)
			}
			if !WaitForCoordWake(coordCtx, runner) {
				return
			}
			userMsg = DefaultCoordContinuationPrompt + BuildCoordStepMenu(runner)
		}
	}()
	return func() {
		cancel()
		timer := time.NewTimer(cfg.cleanupTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C: // coverage-note: coord LLM timeout branch; requires a slow LLM that outlives cleanupTimeout in a real process
			// Coord LLM didn't acknowledge cancellation - let it leak
			// rather than block CLI exit. The CLI process exit will
			// reap the goroutine.
		}
	}
}
